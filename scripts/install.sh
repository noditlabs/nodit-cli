#!/bin/sh
#
# Nodit CLI installer.
#
#   curl -fsSL https://raw.githubusercontent.com/noditlabs/nodit-cli/main/scripts/install.sh | sh
#
# The script is piped into a shell, so it reads the archive checksum from the release and refuses
# to install a binary that does not match. Environment overrides:
#
#   NODIT_VERSION          tag to install, such as v0.1.0 (default: the latest release)
#   NODIT_INSTALL_DIR      where the binary lands (default: $HOME/.local/bin)
#   NODIT_NO_MODIFY_PATH   set to 1 to leave shell startup files alone

set -eu

REPO="noditlabs/nodit-cli"
VERSION="${NODIT_VERSION:-}"
INSTALL_DIR="${NODIT_INSTALL_DIR:-$HOME/.local/bin}"
MARKER="# added by nodit-cli installer"

fail() {
	echo "install: $1" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

need uname
need mkdir
need tar
command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 ||
	fail "curl or wget is required"

# Fails on a non-2xx status instead of writing an error page where a payload is expected.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	else
		wget -qO- "$1"
	fi
}

fetch_to() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	else
		wget -qO "$2" "$1"
	fi
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported operating system: $(uname -s). Windows uses scripts/install.ps1" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture: $(uname -m)" ;;
esac

# The release builds darwin on both architectures but linux on amd64 only, so an arm64 Linux host
# would otherwise download a 404 page and install it as a binary.
if [ "$os" = linux ] && [ "$arch" != amd64 ]; then
	fail "no linux/$arch build is published. Build from source: see README"
fi

if [ -z "$VERSION" ]; then
	VERSION=$(fetch "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$VERSION" ] || fail "cannot determine the latest release. Set NODIT_VERSION to a tag"
fi

name="nodit-${os}-${arch}"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading $name $VERSION"
fetch_to "$base/$name.tar.gz" "$tmp/$name.tar.gz" || fail "cannot download $name.tar.gz for $VERSION"
fetch_to "$base/checksums.txt" "$tmp/checksums.txt" || fail "cannot download checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)
else
	fail "sha256sum or shasum is required to verify the download"
fi

# checksums.txt lists names as ./nodit-os-arch.tar.gz because it is produced by sha256sum ./*.
expected=$(sed -n "s|^\([0-9a-f]\{64\}\)[[:space:]]*\.\{0,1\}/\{0,1\}$name\.tar\.gz\$|\1|p" \
	"$tmp/checksums.txt" | head -n 1)
[ -n "$expected" ] || fail "checksums.txt has no entry for $name.tar.gz"
[ "$actual" = "$expected" ] || fail "checksum mismatch for $name.tar.gz — refusing to install"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
[ -f "$tmp/$name/nodit" ] || fail "the archive does not contain $name/nodit"

# An older or unreadable binary leaves this empty, which must not stop the install.
installed_version() {
	[ -x "$1" ] || return 0
	"$1" version --output json 2>/dev/null |
		sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

chmod +x "$tmp/$name/nodit"
# Run it before it replaces anything. A binary that cannot start here would otherwise be reported as
# installed, and a working one would already have been overwritten.
"$tmp/$name/nodit" version --output json >/dev/null 2>&1 ||
	fail "the downloaded binary does not run on this machine; nothing was changed"

previous=$(installed_version "$INSTALL_DIR/nodit")
current=$(installed_version "$tmp/$name/nodit")
[ -n "$current" ] || current="$VERSION"

mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
# Replace by rename so a running nodit keeps its open file and no half-written binary is left.
mv "$tmp/$name/nodit" "$INSTALL_DIR/nodit.new" || fail "cannot write to $INSTALL_DIR"
mv "$INSTALL_DIR/nodit.new" "$INSTALL_DIR/nodit"

if [ -z "$previous" ]; then
	echo "Installed nodit $current at $INSTALL_DIR/nodit"
elif [ "$previous" = "$current" ]; then
	echo "Reinstalled nodit $current at $INSTALL_DIR/nodit"
else
	echo "Updated nodit $previous -> $current at $INSTALL_DIR/nodit"
fi

# Startup file of the login shell. A pipe leaves no tty, so $SHELL is the only hint available.
startup_file() {
	case "${SHELL:-}" in
	*/zsh) echo "${ZDOTDIR:-$HOME}/.zshrc" ;;
	*/bash)
		# macOS terminals start a login shell, which reads .bash_profile and not .bashrc.
		if [ "$os" = darwin ] && [ -f "$HOME/.bash_profile" ]; then
			echo "$HOME/.bash_profile"
		else
			echo "$HOME/.bashrc"
		fi
		;;
	*/fish) echo "$HOME/.config/fish/config.fish" ;;
	*) echo "" ;;
	esac
}

# fish does not take `export`, and a line the caller cannot paste is worse than none.
path_command() {
	case "${SHELL:-}" in
	*/fish) echo "fish_add_path $INSTALL_DIR" ;;
	*) echo "export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
	esac
}

# Nothing here is run for the caller: completion means editing a startup file and a shell the
# installer is not running in, and PATH is already as far into those files as an installer should go.
# $SHELL is the only hint available, so an unrecognized shell says nothing rather than guessing.
completion_setup() {
	case "${SHELL:-}" in
	*/zsh)
		echo "Enable tab completion, once:"
		echo "  mkdir -p ~/.zfunc"
		echo "  nodit completion zsh > ~/.zfunc/_nodit"
		echo "Then add these two lines to ${rc:-~/.zshrc}:"
		echo "  fpath=(~/.zfunc \$fpath)"
		echo "  autoload -Uz compinit && compinit"
		;;
	*/bash)
		echo "Enable tab completion, once:"
		echo "  mkdir -p ~/.bash_completion.d"
		echo "  nodit completion bash > ~/.bash_completion.d/nodit"
		echo "Then add this line to ${rc:-~/.bashrc}:"
		echo "  source ~/.bash_completion.d/nodit"
		;;
	*/fish)
		echo "Enable tab completion, once:"
		echo "  mkdir -p ~/.config/fish/completions"
		echo "  nodit completion fish > ~/.config/fish/completions/nodit.fish"
		;;
	*) return ;;
	esac
	echo "Open a new shell for it to take effect. Other shells: nodit completion --help"
}

# The next step comes last, so it is what the caller is left looking at.
next_step() {
	completion_setup
	echo "Then run: nodit auth login, or nodit --help"
	exit 0
}

case ":$PATH:" in
*":$INSTALL_DIR:"*) next_step ;;
esac

rc=$(startup_file)
if [ "${NODIT_NO_MODIFY_PATH:-}" = 1 ] || [ -z "$rc" ]; then
	echo "Add it to PATH:  $(path_command)"
	next_step
fi

# Rerunning the installer must not stack duplicate entries.
if [ -f "$rc" ] && grep -qF "$MARKER" "$rc"; then
	echo "$rc already sets PATH for nodit. Open a new shell to pick it up."
	next_step
fi

mkdir -p "$(dirname "$rc")"
case "$rc" in
*/config.fish) printf '\n%s\nfish_add_path %s\n' "$MARKER" "$INSTALL_DIR" >>"$rc" ;;
*) printf '\n%s\nexport PATH="%s:$PATH"\n' "$MARKER" "$INSTALL_DIR" >>"$rc" ;;
esac

echo "Added $INSTALL_DIR to PATH in $rc. Open a new shell, or run:"
echo "  $(path_command)"
next_step
