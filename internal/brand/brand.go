// Package brand holds the product identity shown in the UI.
//
// This file is the only place where the default product name may appear as a
// literal. Templates and messages must read the name from a Brand value.
package brand

// Brand is the identity shown in page titles, the header and notifications.
type Brand struct {
	// Name is the product name.
	Name string
	// Accent is the accent color as a CSS hex value.
	Accent string
	// PoweredBy controls the "Powered by" footer line.
	PoweredBy bool
}

// Default is the built-in brand.
var Default = Brand{
	Name:      "vexil",
	Accent:    "#6366F1",
	PoweredBy: true,
}
