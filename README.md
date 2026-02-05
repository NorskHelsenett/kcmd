# kcmd

A terminal user interface (TUI) for executing commands inside Kubernetes pods with an interactive shell experience, **without requiring an interactive shell connection**.

Unlike traditional `kubectl exec -it` which requires a TTY and keeps an open connection, kcmd executes each command independently while maintaining state (working directory, command history) on the client side. This provides a shell-like experience with better reliability and additional features like scrollable output, line numbers, and clipboard integration.

## Key Difference from kubectl exec -it

- **No persistent connection**: Each command runs independently via `kubectl exec` without TTY allocation
- **Client-side state**: Working directory tracking, history, and autocomplete are managed locally
- **Non-blocking**: Commands complete and disconnect, preventing hung connections
- **Enhanced output**: Scrollable, numbered output with copy functionality
- **Works with non-TTY environments**: Compatible with containers that don't support interactive shells

## Features

### Interactive Pod Selection
- Select namespace from available namespaces
- Choose resource type (pod, deployment, or statefulset)
- Pick specific pod or workload
- Select container if pod has multiple containers

### Interactive Shell

Once connected to a pod container, you get an interactive shell **experience** with:

- **Command execution** - Run any shell command in the container (each as a separate kubectl exec)
- **Working directory tracking** - Use `cd` to change directories; all subsequent commands run in that context
- **Command history** - Navigate with up/down arrow keys through previously executed commands
- **Output scrolling** - Scroll through output with PgUp/PgDn or arrow keys
- **Line numbers** - All output is numbered for easy reference
- **Tab completion** - Autocomplete filesystem paths and words from output
- **Copy to clipboard** - Copy specific line ranges from output

**Important**: This is not a persistent shell session. Each command is executed independently, but the application maintains state to provide a seamless shell-like experience.

### Tab Completion

The Tab key provides intelligent autocomplete:

- **Filesystem paths** - `cat /app/re<Tab>` completes to `/app/responses/`
- **Partial matches** - `cat /app/responses/4e<Tab>` completes to matching files/directories
- **Word completion** - Words from command output are available for completion
- **Context aware** - Autocomplete resets when changing directories

### Copy Command

Copy output lines to clipboard using the `/copy` command:

```
/copy 42          Copy line 42
/copy 10,20       Copy lines 10 through 20
/copy 100-150     Copy lines 100 through 150 (alternative syntax)
```

Supports clipboard utilities:
- macOS: `pbcopy`
- Linux: `xclip` or `xsel`
- Windows: `clip.exe`

### Keyboard Shortcuts

**In selection mode:**
- `Enter` - Select item
- `/` - Filter list
- `Esc` - Go back to previous step
- `q` - Quit application

**In shell mode:**
- `Enter` - Execute command
- `Tab` - Autocomplete path or word
- `Up/Down` - Navigate command history
- `PgUp/PgDn` - Scroll output
- `Ctrl+R` - Retarget (choose new pod)
- `q` - Quit application
- `clear` - Clear output buffer

## Installation

### Prerequisites

- Go 1.19 or later
- `kubectl` installed and configured
- Access to a Kubernetes cluster

### Build

```bash
git clone <repository-url>
cd kcmd
go build -o kcmd main.go
```

### Install

```bash
# Option 1: Copy to local bin
cp kcmd /usr/local/bin/

# Option 2: Install with go
go install
```

## Usage

Simply run the command:

```bash
kcmd
```

Follow the interactive prompts to:
1. Select a namespace
2. Choose resource type (pod, deployment, statefulset)
3. Select the specific resource
4. Pick a container (if multiple)
5. Execute commands in the interactive shell

## Examples

### Basic Navigation

```
# Change to a directory
cd /app/logs

# List files (runs in /app/logs context)
ls -la

# View file (still in /app/logs context)
cat access.log
```

### Using Autocomplete

```
# Type partial path and press Tab
cat /app/re<Tab>           # Completes to /app/responses/
cat /app/responses/4e<Tab>  # Completes to matching file/directory
```

### Copying Output

```
# Run a command that produces output
ls -la

# Copy specific lines to clipboard
/copy 5,15    # Copies lines 5-15 from the output
```

### Working with Multiple Directories

```
# Change directory
cd /app/data

# Work with files
ls *.json

# Change to different directory (autocomplete resets)
cd /var/log

# Autocomplete now suggests files from /var/log
tail -f app<Tab>
```

## Technical Details

### Command Execution Model

kcmd does **not** use `kubectl exec -it` or maintain a persistent shell connection. Instead:

- Each command is executed as a separate `kubectl exec` call without TTY allocation
- Commands use `sh -lc` for execution to support pipes, redirects, and shell features
- Client-side state tracking simulates a persistent session
- Working directory changes are implemented by prefixing commands with `cd <dir> &&`

This approach provides several advantages:
- **Reliability**: No risk of hung or dropped connections
- **Compatibility**: Works with containers that don't support interactive TTY
- **Enhanced features**: Scrollable output, line numbering, and clipboard integration
- **State preservation**: Directory context and history maintained across command executions

### Command Execution

Commands are executed using `kubectl exec` with the following behavior:
- Non-interactive mode (no TTY allocation)
- Uses `sh -lc` for command execution to support pipes and redirects
- Respects the current working directory set by `cd` commands

### Directory Tracking

The `cd` command is handled locally without remote execution:
- Directory changes are tracked in the application state
- Subsequent commands are prefixed with `cd <directory> &&`
- Works with absolute paths, relative paths, and home directory (`~`)

### Output Processing

- All command output is captured and displayed with line numbers
- Standard output and standard error are shown separately
- Words longer than 2 characters are extracted for autocomplete
- Output persists across commands until cleared

## Limitations

- **No persistent shell state**: Each command runs independently; shell variables, functions, and session state don't persist
- **Commands requiring TTY interaction**: Tools like `vim`, `nano`, `top`, or any interactive programs will not work properly
- **Tab completion latency**: Filesystem autocomplete queries the pod, which adds slight delay
- **Autocomplete scope**: Dictionary is cleared when changing directories
- **No session persistence**: History and state are lost when the application exits
- **Performance**: Not suitable for high-frequency command execution due to kubectl overhead per command

## License

MIT
