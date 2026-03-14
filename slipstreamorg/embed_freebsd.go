//go:build embed_slipstream && freebsd && amd64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-freebsd-amd64
var BinaryData []byte
