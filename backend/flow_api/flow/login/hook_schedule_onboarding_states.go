package login

import (
	"fmt"
	"github.com/gofrs/uuid"
	"github.com/teamhanko/hanko/backend/flow_api/flow/device_trust"
	"github.com/teamhanko/hanko/backend/flow_api/flow/shared"
	"github.com/teamhanko/hanko/backend/flow_api/services"
	"github.com/teamhanko/hanko/backend/flowpilot"
)

type ScheduleOnboardingStates struct {
	shared.Action
}

func (h ScheduleOnboardingStates) Execute(c flowpilot.HookExecutionContext) error {
	if c.Stash().Get(shared.StashPathLoginOnboardingScheduled).Bool() {
		return nil
	}

	if err := c.Stash().Set(shared.StashPathLoginOnboardingScheduled, true); err != nil {
		return fmt.Errorf("failed to set login_onboarding_scheduled to the stash: %w", err)
	}

	mfaUsageStates := h.determineMFAUsageStates(c)
	userDetailOnboardingStates := h.determineUserDetailOnboardingStates(c)
	credentialOnboardingStates := h.determineCredentialOnboardingStates(c)

	c.ScheduleStates(mfaUsageStates...)

	if c.Stash().Get(shared.StashPathPasswordRecoveryPending).Bool() {
		c.ScheduleStates(shared.StateLoginPasswordRecovery)
	}

	c.ScheduleStates(userDetailOnboardingStates...)
	c.ScheduleStates(credentialOnboardingStates...)

	err := c.ExecuteHook(device_trust.ScheduleTrustDeviceState{})
	if err != nil {
		return err
	}

	c.ScheduleStates(shared.StateSuccess)
	return nil
}

func (h ScheduleOnboardingStates) determineMFAUsageStates(c flowpilot.HookExecutionContext) []flowpilot.StateName {
	deps := h.GetDeps(c)
	cfg := deps.Cfg
	result := make([]flowpilot.StateName, 0)

	if !cfg.MFA.Enabled {
		return result
	}

	if c.Stash().Get(shared.StashPathLoginMethod).String() == "passkey" {
		return result
	}

	deviceTrustService := services.DeviceTrustService{
		Persister:   deps.Persister.GetTrustedDevicePersisterWithConnection(deps.Tx),
		Cfg:         deps.Cfg,
		HttpContext: deps.HttpContext,
	}

	userID := uuid.FromStringOrNil(c.Stash().Get(shared.StashPathUserID).String())

	if deviceTrustService.CheckDeviceTrust(userID) {
		// The device is trusted, so MFA can be skipped.
		return result
	}

	userHasSecurityKey := c.Stash().Get(shared.StashPathUserHasSecurityKey).Bool()
	userHasOTPSecret := c.Stash().Get(shared.StashPathUserHasOTPSecret).Bool()
	attachmentSupported := c.Stash().Get(shared.StashPathSecurityKeyAttachmentSupported).Bool()
	userCanUseOTP := cfg.MFA.TOTP.Enabled && userHasOTPSecret

	if cfg.MFA.SecurityKeys.Enabled && userHasSecurityKey {
		switch {
		case !attachmentSupported && !userCanUseOTP:
			c.SetFlowError(shared.ErrorPlatformAuthenticatorRequired)
			result = append(result, shared.StateError)
		case attachmentSupported:
			result = append(result, shared.StateLoginSecurityKey)
		case userCanUseOTP:
			result = append(result, shared.StateLoginOTP)
		}
	} else if userCanUseOTP {
		result = append(result, shared.StateLoginOTP)
	}

	return result
}

func (h ScheduleOnboardingStates) determineCredentialOnboardingStates(c flowpilot.HookExecutionContext) []flowpilot.StateName {
	deps := h.GetDeps(c)
	cfg := deps.Cfg
	result := make([]flowpilot.StateName, 0)

	hasPassword := c.Stash().Get(shared.StashPathUserHasPassword).Bool()
	hasPasskey := c.Stash().Get(shared.StashPathUserHasPasskey).Bool()
	webauthnAvailable := c.Stash().Get(shared.StashPathWebauthnAvailable).Bool()
	passkeyEnabled := webauthnAvailable && deps.Cfg.Passkey.Enabled
	passwordEnabled := deps.Cfg.Password.Enabled
	passwordAndPasskeyEnabled := passkeyEnabled && passwordEnabled

	alwaysAcquirePasskey := cfg.Passkey.AcquireOnLogin == "always"
	alwaysAcquirePassword := cfg.Password.AcquireOnLogin == "always"
	conditionalAcquirePasskey := cfg.Passkey.AcquireOnLogin == "conditional"
	conditionalAcquirePassword := cfg.Password.AcquireOnLogin == "conditional"
	neverAcquirePasskey := cfg.Passkey.AcquireOnLogin == "never"
	neverAcquirePassword := cfg.Password.AcquireOnLogin == "never"

	if c.Stash().Get(shared.StashPathPasswordRecoveryPending).Bool() {
		// never acquire password, when recovery has been initiated
		neverAcquirePassword = true
	}

	if passwordAndPasskeyEnabled {
		if alwaysAcquirePasskey && alwaysAcquirePassword {
			if !hasPasskey && !hasPassword {
				if !cfg.Password.Optional && cfg.Passkey.Optional {
					result = append(result, shared.StatePasswordCreation, shared.StateOnboardingCreatePasskey)
				} else {
					result = append(result, shared.StateOnboardingCreatePasskey, shared.StatePasswordCreation)
				}
			} else if hasPasskey && !hasPassword {
				result = append(result, shared.StatePasswordCreation)
			} else if !hasPasskey && hasPassword {
				result = append(result, shared.StateOnboardingCreatePasskey)
			}
		} else if alwaysAcquirePasskey && conditionalAcquirePassword {
			if !hasPasskey && !hasPassword {
				result = append(result, shared.StateOnboardingCreatePasskey) // skip should lead to password onboarding
			} else if !hasPasskey && hasPassword {
				result = append(result, shared.StateOnboardingCreatePasskey)
			}
		} else if conditionalAcquirePasskey && alwaysAcquirePassword {
			if !hasPasskey && !hasPassword {
				result = append(result, shared.StatePasswordCreation) // skip should lead to passkey onboarding
			} else if hasPasskey && !hasPassword {
				result = append(result, shared.StatePasswordCreation)
			}
		} else if conditionalAcquirePasskey && conditionalAcquirePassword {
			if !hasPasskey && !hasPassword {
				result = append(result, shared.StateCredentialOnboardingChooser) // credential_onboarding_chooser can be skipped
			}
		} else if conditionalAcquirePasskey && neverAcquirePassword {
			if !hasPasskey && !hasPassword {
				result = append(result, shared.StateOnboardingCreatePasskey)
			}
		} else if neverAcquirePasskey && conditionalAcquirePassword {
			if !hasPasskey && !hasPassword {
				result = append(result, shared.StatePasswordCreation)
			}
		} else if neverAcquirePasskey && alwaysAcquirePassword {
			if !hasPassword {
				result = append(result, shared.StatePasswordCreation)
			}
		} else if alwaysAcquirePasskey && neverAcquirePassword {
			if !hasPasskey {
				result = append(result, shared.StateOnboardingCreatePasskey)
			}
		}
	} else if passkeyEnabled && (alwaysAcquirePasskey || conditionalAcquirePasskey) {
		if !hasPasskey {
			result = append(result, shared.StateOnboardingCreatePasskey)
		}
	} else if passwordEnabled && (alwaysAcquirePassword || conditionalAcquirePassword) {
		if !hasPassword {
			result = append(result, shared.StatePasswordCreation)
		}
	}

	return result
}

func (h ScheduleOnboardingStates) determineUserDetailOnboardingStates(c flowpilot.HookExecutionContext) []flowpilot.StateName {
	deps := h.GetDeps(c)
	cfg := deps.Cfg
	result := make([]flowpilot.StateName, 0)

	userHasUsername := c.Stash().Get(shared.StashPathUserHasUsername).Bool()
	userHasEmail := c.Stash().Get(shared.StashPathUserHasEmails).Bool()
	acquireUsername := !userHasUsername && cfg.Username.Enabled && cfg.Username.AcquireOnLogin
	acquireEmail := !userHasEmail && cfg.Email.Enabled && cfg.Email.AcquireOnLogin

	if acquireUsername && acquireEmail {
		result = append(result, shared.StateOnboardingUsername, shared.StateOnboardingEmail)
	} else if acquireUsername {
		result = append(result, shared.StateOnboardingUsername)
	} else if acquireEmail {
		result = append(result, shared.StateOnboardingEmail)
	}

	return result
}
