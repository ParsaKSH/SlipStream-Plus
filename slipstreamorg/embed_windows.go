//go:build embed_slipstream && windows && amd64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client.exe
var BinaryData []byte
