# Records docs/demo.gif: local-ci's own CI running on this Mac.
# Needs `localci setup` done, nobody else's localci up on the VM, tmux, and
# ci.yml with workflow_dispatch on the default branch. Run from the repo root:
#   make build && vhs docs/demo.tape
Output docs/demo.gif

Require gh
Require limactl
Require tmux

Set Shell "bash"
Set FontSize 15
Set Width 1100
Set Height 640
Set Padding 16
Set TypingSpeed 40ms
# LocalCI's own logs off: the runner's output tells the story.
Env LOCALCI_LOG_LEVEL "warn"
Env GH_PAGER "cat"
Env PS1 "$ "

# Two panes: localci up on top, gh below. A private tmux server, no user config.
Hide
Type "tmux -L localci-demo -f /dev/null new-session 'bash --norc' \; set status off \; set default-command 'bash --norc'" Enter
Sleep 1s
Show

Type "bin/localci up -repo harrydt/local-ci" Enter
Wait+Screen@90s /Listening for Jobs/
Sleep 1s

Hide
Ctrl+B
Type '"'
Sleep 1s
Show

Type "gh workflow run ci.yml" Enter
Wait+Screen@120s /Running job: check/
Sleep 2s

# Skip the seconds the job takes.
Hide
Wait+Screen@600s /Job check completed with result/
Show
Sleep 2s

Type "gh run list -w ci.yml -L 1" Enter
Sleep 4s

# Ctrl-C localci up so it unroutes, then stop the private tmux server.
Hide
Ctrl+B
Up
Ctrl+C
Sleep 5s
Type "tmux -L localci-demo kill-server" Enter
Sleep 1s
