// Package scripts embeds the shell scripts the localci binary runs, so a
// brew-installed binary doesn't need a checkout of this repo.
package scripts

import _ "embed"

//go:embed setup-vm.sh
var SetupVM string
