# Package the prebuilt static Go binary without rewriting or stripping it.
%global __os_install_post %{nil}
%global _build_id_links none
%global debug_package %{nil}

Name: mcp-native-diagnostics
Version: %{package_version}
Release: 1
Summary: Local MCP server for native diagnostics
License: Apache-2.0
URL: https://github.com/Morditux/traceanvil-mcp
AutoReqProv: no

%description
MCP stdio server for Valgrind, GDB, sanitizers, CTest and profiling.
It launches only administrator-declared local targets; no daemon or
privileged helper is installed.

%install
mkdir -p %{buildroot}
cp -a %{_sourcedir}/. %{buildroot}/

%files
%defattr(-,root,root,-)
/usr/bin/mcp-native-diagnostics
%dir /usr/share/doc/mcp-native-diagnostics
%doc /usr/share/doc/mcp-native-diagnostics/README.md
%doc /usr/share/doc/mcp-native-diagnostics/SECURITY.md
%doc /usr/share/doc/mcp-native-diagnostics/config.example.json
%doc /usr/share/doc/mcp-native-diagnostics/docs
%license /usr/share/doc/mcp-native-diagnostics/LICENSE
