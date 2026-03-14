//go:build embed_slipstream && windows && arm64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-windows-arm64.exe
var BinaryData []byte
