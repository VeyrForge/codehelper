//go:build !ch_modules

package product

// Default full bundle: every shipping module on; team remains opt-in.
const (
	selectMode = false
	editOn     = true
	checkOn    = true
	browserOn  = true
	opsOn      = true
	teamOn     = false
)
