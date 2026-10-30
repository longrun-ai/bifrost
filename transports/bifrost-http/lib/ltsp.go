package lib

import "github.com/maximhq/bifrost/transports/v2/bifrost-http/ltsp"

// LTSP is set once before core initialization in the enabled LTSP deployment.
// Unset builds retain upstream behavior.
var LTSP *ltsp.Manager
