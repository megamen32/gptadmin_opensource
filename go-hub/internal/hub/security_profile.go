package hub

import (
	"errors"
	"strings"
)

const (
	processSecurityNormal  = "normal"
	processSecurityMaximum = "maximum"
	processSecurityCustom  = "custom"
)

// Process security is independent from the bearer/OAuth preset.
type processSecurityProfile struct {
	Mode            string `json:"mode"`
	NoNewPrivileges bool   `json:"no_new_privileges"`
	PrivateTmp      bool   `json:"private_tmp"`
	ProtectSystem   bool   `json:"protect_system"`
	ProtectHome     bool   `json:"protect_home"`
	AllowPrivileged bool   `json:"allow_privileged_execution"`
}

// Bearer verification is deliberately separate from OS process isolation.
// Signature verification is unconditional; these flags control the claims and
// protocol checks around an otherwise validly signed bearer.
type bearerSecurityProfile struct {
	Mode                     string `json:"mode"`
	RequireIssuer            bool   `json:"require_issuer"`
	RequireAudience          bool   `json:"require_audience"`
	RequireResource          bool   `json:"require_resource"`
	RequireScope             bool   `json:"require_scope"`
	RequireSubject           bool   `json:"require_subject"`
	RequireIssuedAt          bool   `json:"require_issued_at"`
	RequireExpiry            bool   `json:"require_expiry"`
	RequirePKCE              bool   `json:"require_pkce"`
	EnforceTokenLifecycle    bool   `json:"enforce_token_lifecycle"`
	EnforceRedirectAllowlist bool   `json:"enforce_redirect_allowlist"`
	EnforceResourceAllowlist bool   `json:"enforce_resource_allowlist"`
}

func maximumBearerSecurityProfile() bearerSecurityProfile {
	return bearerSecurityProfile{
		Mode: processSecurityMaximum, RequireIssuer: true, RequireAudience: true,
		RequireResource: true, RequireScope: true, RequireSubject: true,
		RequireIssuedAt: true, RequireExpiry: true, RequirePKCE: true,
		EnforceTokenLifecycle: true, EnforceRedirectAllowlist: true,
		EnforceResourceAllowlist: true,
	}
}

func defaultBearerSecurityProfile() bearerSecurityProfile {
	return legacyBearerSecurityProfile()
}

func legacyBearerSecurityProfile() bearerSecurityProfile {
	// This is the established normal contract: signature, audience, resource,
	// scope, subject, issued-at, expiry, PKCE and managed-token lifecycle remain
	// checked, while a legacy token may omit issuer and the existing explicit
	// permissive redirect/resource flags retain their meaning.
	return bearerSecurityProfile{
		Mode: processSecurityNormal, RequireAudience: true, RequireResource: true,
		RequireScope: true, RequireSubject: true, RequireIssuedAt: true,
		RequireExpiry: true, RequirePKCE: true, EnforceTokenLifecycle: true,
	}
}

func validateBearerSecurityProfile(profile bearerSecurityProfile) error {
	switch strings.ToLower(strings.TrimSpace(profile.Mode)) {
	case processSecurityNormal:
		return nil
	case processSecurityMaximum:
		if profile != maximumBearerSecurityProfile() {
			return errors.New("maximum bearer profile requires every bearer and OAuth check")
		}
	case processSecurityCustom:
		return nil
	default:
		return errors.New("bearer security mode must be normal, maximum or custom")
	}
	return nil
}

func (s *Server) effectiveBearerSecurityProfile() bearerSecurityProfile {
	p := s.securitySnapshot().Bearer
	if strings.ToLower(strings.TrimSpace(p.Mode)) == processSecurityNormal {
		if s.cfg.RelaxAuthChecks {
			// Emergency compatibility remains an explicit escape hatch, but the
			// HMAC/JWT signature check is never disabled.
			p = bearerSecurityProfile{Mode: processSecurityCustom}
		} else {
			// Preserve the established normal auth contract.
			p = legacyBearerSecurityProfile()
		}
	}
	return p
}

func defaultProcessSecurityProfile() processSecurityProfile {
	return processSecurityProfile{Mode: processSecurityNormal, AllowPrivileged: true}
}

func validateProcessSecurityProfile(profile processSecurityProfile) error {
	switch strings.ToLower(strings.TrimSpace(profile.Mode)) {
	case processSecurityNormal:
		if profile.NoNewPrivileges || profile.PrivateTmp || profile.ProtectSystem || profile.ProtectHome {
			return errors.New("normal process profile cannot enable hardening flags")
		}
	case processSecurityMaximum:
		if !profile.NoNewPrivileges || !profile.PrivateTmp || !profile.ProtectSystem || !profile.ProtectHome || profile.AllowPrivileged {
			return errors.New("maximum process profile requires all hardening flags and disables privileged execution")
		}
	case processSecurityCustom:
		// Refusing privileged execution must be backed by the systemd
		// no-new-privileges boundary; otherwise the UI would claim a control
		// that the generated unit does not actually enforce.
		if !profile.AllowPrivileged && !profile.NoNewPrivileges {
			return errors.New("custom profile denying privileged execution requires no_new_privileges")
		}
	default:
		return errors.New("process security mode must be normal, maximum or custom")
	}
	return nil
}
