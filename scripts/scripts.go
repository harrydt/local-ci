// Package scripts embeds the shell scripts the localci binary runs, so a
// brew-installed binary doesn't need a checkout of this repo.
package scripts

import _ "embed"

//go:embed setup-vm.sh
var SetupVM string

// InstallCache runs inside the VM to turn the local Actions cache on or off.
//
//go:embed install-cache.sh
var InstallCache string
