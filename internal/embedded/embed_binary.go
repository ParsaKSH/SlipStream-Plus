//go:build embed_slipstream

package embedded

import _ "embed"

//go:embed slipstream-client
var embeddedBinary []byte

func init() {
	BinaryData = embeddedBinary
}
