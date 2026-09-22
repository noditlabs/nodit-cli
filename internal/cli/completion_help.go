package cli

import "github.com/spf13/cobra"

// cobra's generated text points at ${fpath[1]} and $(brew --prefix), which resolve differently per
// machine and expand to a system path when Homebrew is absent. These paths are under $HOME, so they
// need no sudo and read the same everywhere. Each shell's prerequisite is stated next to the
// command that needs it, because a script saved in the right place still does nothing without it.
const completionIntro = `Generate a shell completion script for nodit.

The script goes to standard output. Save it where your shell looks for completions, then start a
new shell. Each shell has its own prerequisite.
`

const zshSetup = `  mkdir -p ~/.zfunc
  nodit completion zsh > ~/.zfunc/_nodit

zsh reads the file only after compinit runs. Add both lines to ~/.zshrc:

  fpath=(~/.zfunc $fpath)
  autoload -Uz compinit && compinit
`

const bashSetup = `  mkdir -p ~/.bash_completion.d
  nodit completion bash > ~/.bash_completion.d/nodit

Sourcing the file directly needs no bash-completion package. Add this to ~/.bashrc, or to
~/.bash_profile on macOS, where terminals start a login shell:

  source ~/.bash_completion.d/nodit
`

const fishSetup = `  mkdir -p ~/.config/fish/completions
  nodit completion fish > ~/.config/fish/completions/nodit.fish

fish reads that directory on its own, so nothing else is needed.
`

const powershellSetup = `  nodit completion powershell | Out-String | Invoke-Expression

That loads it for the current session. Add the same line to the profile at $PROFILE to load it in
every session.
`

// The parent carries every shell, the way a reader who has not picked one needs it; each leaf
// repeats only its own.
func completionHelp() (string, map[string]string) {
	setups := map[string]string{
		"zsh": zshSetup, "bash": bashSetup, "fish": fishSetup, "powershell": powershellSetup,
	}
	long := completionIntro
	for _, name := range []string{"zsh", "bash", "fish", "powershell"} {
		long += "\n" + name + "\n\n" + setups[name]
	}
	leaves := map[string]string{}
	for name, setup := range setups {
		leaves[name] = "Generate the " + name + " completion script for nodit.\n\n" + setup
	}
	return long, leaves
}

func rewriteCompletionHelp(root *cobra.Command) {
	long, leaves := completionHelp()
	for _, cmd := range root.Commands() {
		if cmd.Name() != "completion" {
			continue
		}
		cmd.Long = long
		for _, shell := range cmd.Commands() {
			if text, ok := leaves[shell.Name()]; ok {
				shell.Long = text
			}
		}
	}
}
