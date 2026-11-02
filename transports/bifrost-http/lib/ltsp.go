package lib

import (
	"errors"
	"os"

	"github.com/maximhq/bifrost/transports/v2/bifrost-http/ltsp"
)

// LTSP is set once before core initialization in the enabled LTSP deployment.
// Unset builds retain upstream behavior.
var LTSP *ltsp.Manager

// RequireLTSPInferenceAuth validates the enabled LTSP deployment at config load
// and before a config API update mutates live or persisted settings.
func RequireLTSPInferenceAuth(enabled bool) error {
	if (LTSP != nil || os.Getenv("LTSP_GATEWAY_STATE") != "") && !enabled {
		return errors.New("LTSP LLM API 网关配置错误：必须启用下游鉴权（enforce_auth_on_inference=true）")
	}
	return nil
}
