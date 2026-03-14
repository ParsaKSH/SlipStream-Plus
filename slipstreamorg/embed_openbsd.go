//go:build embed_slipstream && openbsd && amd64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-openbsd-amd64
var BinaryData []byte
