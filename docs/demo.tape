# Records docs/demo.gif: local-ci's own CI running on this Mac.
# Needs `localci setup` done, nobody else's localci up on the VM, and ci.yml
# with workflow_dispatch on the default branch. Run from the repo root:
#   make build && vhs docs/demo.tape
Output docs/demo.gif

Require gh
Require limactl

Set Shell "bash"
Set FontSize 15
Set Width 1100
Set Height 560
Set Padding 16
Set TypingSpeed 40ms
# LocalCI's own logs off: the runner's output tells the story.
Env LOCALCI_LOG_LEVEL "warn"

Hide
Type "export PS1='$ ' && clear" Enter
Show

Type "bin/localci up -repo harrydt/local-ci &" Enter
Wait+Screen@90s /Listening for Jobs/
Sleep 1s

Type "gh workflow run ci.yml" Enter
Wait+Screen@120s /Running job: check/
Sleep 2s

# Skip the minute or two the job takes.
Hide
Wait+Screen@600s /Job check completed with result/
Show
Sleep 2s

Type "gh run list -w ci.yml -L 1" Enter
Sleep 4s

Hide
Type "kill %1; wait" Enter
Sleep 5s
