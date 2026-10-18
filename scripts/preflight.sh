#!/usr/bin/env bash
#
# Read-only inventory of the development machine.
#
# This script never installs packages, never changes kernel settings and never
# executes a user program. Missing tools are reported without failing, so the
# output can be recorded in docs/compatibility.md or pasted into an issue.
set -u

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
	cat <<'EOF'
Usage: preflight.sh [--help]

Read-only inventory of the development machine.

This script never installs packages, never changes kernel settings and never
executes a user program. Missing tools are reported without failing, so the
output can be recorded in docs/compatibility.md or pasted into an issue.
EOF
	exit 0
fi

section() {
	printf '\n== %s\n' "$1"
}

# print_versioned prints the resolved path and the first line of --version.
print_versioned() {
	local tool="$1" path first
	if ! path="$(command -v "$tool" 2>/dev/null)"; then
		printf '  %-22s %s\n' "$tool" "MISSING"
		return
	fi
	first="$("$tool" --version 2>&1 | head -n 1)"
	printf '  %-22s %s\n' "$tool" "$path"
	printf '  %-22s %s\n' "" "$first"
}

# print_path_only is used when running the tool would be unsafe or confusing.
print_path_only() {
	local tool="$1" path
	if path="$(command -v "$tool" 2>/dev/null)"; then
		printf '  %-22s %s\n' "$tool" "$path"
	else
		printf '  %-22s %s\n' "$tool" "MISSING"
	fi
}

section "System"
if [[ -r /etc/os-release ]]; then
	# shellcheck disable=SC1091
	. /etc/os-release
	printf '  %-22s %s\n' "os" "${PRETTY_NAME:-unknown}"
fi
printf '  %-22s %s\n' "kernel" "$(uname -r)"
printf '  %-22s %s\n' "arch" "$(uname -m)"

section "Go"
if command -v go >/dev/null 2>&1; then
	printf '  %-22s %s\n' "go" "$(go version)"
	printf '  %-22s %s\n' "GOTOOLCHAIN" "$(go env GOTOOLCHAIN)"
	printf '  %-22s %s\n' "GOVERSION" "$(go env GOVERSION)"
else
	printf '  %-22s %s\n' "go" "MISSING"
fi

section "C/C++ toolchain"
for tool in gcc g++ clang clang++ make cmake ctest ninja; do
	print_versioned "$tool"
done

section "Debuggers"
for tool in gdb gdbserver lldb; do
	print_versioned "$tool"
done

section "LLDB DAP adapter"
found_lldb_dap=0
if command -v lldb-dap >/dev/null 2>&1; then
	print_path_only lldb-dap
	found_lldb_dap=1
fi
if command -v compgen >/dev/null 2>&1; then
	while IFS= read -r candidate; do
		[[ "$candidate" == "lldb-dap" ]] && continue
		print_path_only "$candidate"
		found_lldb_dap=1
	done < <(compgen -c | grep -E '^lldb-dap(-[0-9]+)?$' 2>/dev/null | sort -u)
fi
if ((found_lldb_dap == 0)); then
	printf '  %-22s %s\n' "lldb-dap" "MISSING"
fi

section "Valgrind"
for tool in valgrind vgdb callgrind_annotate ms_print; do
	print_versioned "$tool"
done

section "Profiling"
print_versioned perf

section "Utilities"
for tool in git jq xmllint python3 shellcheck; do
	print_versioned "$tool"
done

section "Kernel security settings (read-only)"
for knob in /proc/sys/kernel/yama/ptrace_scope \
	/proc/sys/kernel/perf_event_paranoid \
	/proc/sys/kernel/kptr_restrict; do
	if [[ -r "$knob" ]]; then
		value="$(cat "$knob" 2>/dev/null)"
	else
		value="unavailable"
	fi
	printf '  %-45s %s\n' "$knob" "${value:-unavailable}"
done

section "Summary for v0.1"
missing=()
for tool in go gcc g++ gdb valgrind; do
	command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if ((${#missing[@]} == 0)); then
	printf '  All v0.1 tools are present.\n'
else
	printf '  Missing v0.1 tools: %s\n' "${missing[*]}"
	printf '  Install them explicitly; the server never installs or escalates privileges.\n'
fi

printf '\nInventory complete. Nothing was modified.\n'
