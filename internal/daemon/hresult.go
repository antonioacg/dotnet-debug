package daemon

import "strings"

// hresultMessages maps common HRESULT error codes from netcoredbg to
// human-readable explanations.
var hresultMessages = map[string]string{
	"0x80070057": "E_INVALIDARG — expression too complex for netcoredbg. Try breaking it into simpler sub-expressions.",
	"0x80004002": "E_NOINTERFACE — expression type not supported. Try evaluating sub-parts individually.",
	"0x80004005": "E_FAIL — general evaluation failure. The expression may use unsupported syntax (casts, string interpolation, complex LINQ).",
	"0x80131509": "COR_E_INVALIDOPERATION — operation not valid in the current state.",
}

// enhanceError appends a human-readable explanation if the error message
// contains a known HRESULT code.
func enhanceError(msg string) string {
	for code, explanation := range hresultMessages {
		if strings.Contains(msg, code) {
			return msg + " (" + explanation + ")"
		}
	}
	return msg
}
