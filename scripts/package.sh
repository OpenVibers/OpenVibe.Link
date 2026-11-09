#!/bin/sh
# Build the friendly Linux and macOS artifacts from the binaries dist.sh just built:
#   .deb, .rpm and a .tar.gz for Linux amd64 and arm64, and a .tar.gz for macOS arm64 and amd64.
# The binaries themselves stay in dist/ for the curl | sh installer and for direct download.
#   scripts/package.sh VERSION
set -eu
VERSION="${1:?usage: scripts/package.sh VERSION}"
cd "$(dirname "$0")/.."

# Package versions cannot carry a leading v or arbitrary git describe text: keep RPM and dpkg happy, and keep the
# version visible to a person. v0.45.0 -> 0.45.0; v0.0.0-dev -> 0.0.0.dev.
pkgver="${VERSION#v}"
pkgver="$(printf '%s' "$pkgver" | sed 's/[^A-Za-z0-9.+~]/./g')"
[ -n "$pkgver" ] || pkgver=0.0.0
# dpkg refuses a version that does not start with a digit; the CI build is version "ci".
case "$pkgver" in [0-9]*) ;; *) pkgver="0.$pkgver" ;; esac

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# docs every package and tarball carries next to the program
docs() {
	dir="$1"
	mkdir -p "$dir"
	cp README.md "$dir/README.md"
	cp docs/install.md "$dir/install.md"
}

# ---- tarballs: Linux and macOS, one per architecture ----
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os="${target%%/*}"
	arch="${target##*/}"
	case "$os/$arch" in
	linux/amd64|linux/arm64) bin="dist/openvibe-link-linux-$arch" ;;
	darwin/amd64|darwin/arm64) bin="dist/openvibe-link-darwin-$arch" ;;
	*) echo "package: no tarball rule for $target" >&2; exit 1 ;;
	esac
	test -s "$bin" || { echo "package: $bin is missing (run scripts/dist.sh first)" >&2; exit 1; }

	root="$work/openvibe-link-$pkgver-$os-$arch"
	mkdir -p "$root"
	# The installer's --local layout: the OS-specific binary name and the plugin bundle beside install.sh.
	cp "$bin" "$root/openvibe-link-$os-$arch"
	cp "$bin" "$root/openvibe-link-$os-$arch"
	cp dist/openvibe-link-plugins.tar.gz "$root/"
	cp install/install.sh "$root/install.sh"
	docs "$root"
	tar -czf "dist/openvibe-link_${pkgver}_${os}_${arch}.tar.gz" -C "$work" "openvibe-link-$pkgver-$os-$arch"
	echo "built dist/openvibe-link_${pkgver}_${os}_${arch}.tar.gz"
done

# ---- .deb: the program and docs; no scripts or network in postinst ----
for arch in amd64 arm64; do
	bin="dist/openvibe-link-linux-$arch"
	test -s "$bin" || { echo "package: $bin is missing" >&2; exit 1; }
	root="$work/deb-$arch"
	mkdir -p "$root/DEBIAN" "$root/usr/local/bin" "$root/usr/share/doc/openvibe-link"
	install -m 0755 "$bin" "$root/usr/local/bin/openvibe-link"
	cp README.md "$root/usr/share/doc/openvibe-link/README.md"
	cp docs/install.md "$root/usr/share/doc/openvibe-link/install.md"
	size="$(du -sk "$root/usr" | cut -f1)"
	cat >"$root/DEBIAN/control" <<EOF
Package: openvibe-link
Version: $pkgver
Section: net
Priority: optional
Architecture: $arch
Maintainer: OpenVibe <Contact@OpenVibe.Network>
Installed-Size: $size
Depends: ca-certificates
Homepage: https://openvibe.link
Description: OpenVibe Link: your machines, connected
 OpenVibe Link runs on your computer or server and connects out to OpenVibe: your agents can use it only as far as
 you allow, and you choose what it offers the network.
EOF
	( cd "$root" && find . -type f -exec md5sum {} + | sed 's|  \./|  |' | sort -k2 >DEBIAN/md5sums )
	dpkg-deb --build --root-owner-group "$root" "dist/openvibe-link_${pkgver}_${arch}.deb" >/dev/null
	echo "built dist/openvibe-link_${pkgver}_${arch}.deb"
done

# ---- .rpm: the same contents, built with rpmbuild; CI installs rpm, a local run skips it with a warning ----
if command -v rpmbuild >/dev/null 2>&1; then
	for pair in amd64:x86_64 arm64:aarch64; do
		arch="${pair%%:*}"
		rpmarch="${pair##*:}"
		bin="dist/openvibe-link-linux-$arch"
		test -s "$bin" || { echo "package: $bin is missing" >&2; exit 1; }
		top="$work/rpm-$rpmarch"
		mkdir -p "$top/SOURCES" "$top/SPECS" "$top/BUILD" "$top/BUILDROOT" "$top/RPMS" "$top/SRPMS"
		cp "$bin" "$top/SOURCES/openvibe-link"
		cp README.md "$top/SOURCES/README.md"
		cp docs/install.md "$top/SOURCES/install.md"
		cat >"$top/SPECS/openvibe-link.spec" <<EOF
Name:           openvibe-link
Version:        $pkgver
Release:        1
Summary:        OpenVibe Link: your machines, connected
License:        AGPL-3.0
URL:            https://openvibe.link
Source0:        openvibe-link
Source1:        README.md
Source2:        install.md
AutoReqProv:    no
%global debug_package %{nil}
%global _build_id_links none

%description
OpenVibe Link runs on your computer or server and connects out to OpenVibe: your agents
can use it only as far as you allow, and you choose what it offers the network. The


%prep

%build

%install
mkdir -p %{buildroot}/usr/local/bin %{buildroot}/usr/share/doc/openvibe-link
install -m 0755 %{SOURCE0} %{buildroot}/usr/local/bin/openvibe-link
install -m 0644 %{SOURCE1} %{buildroot}/usr/share/doc/openvibe-link/README.md
install -m 0644 %{SOURCE2} %{buildroot}/usr/share/doc/openvibe-link/install.md

%files
/usr/local/bin/openvibe-link
/usr/local/bin/openvibe-link
/usr/share/doc/openvibe-link/README.md
/usr/share/doc/openvibe-link/install.md
EOF
		rpmbuild -bb --quiet --define "_topdir $top" --define "_arch $rpmarch" --define "_build_arch $rpmarch" \
			--define "_target_cpu $rpmarch" --define "_target_os linux" "$top/SPECS/openvibe-link.spec" \
			>/dev/null 2>"$work/rpm-$rpmarch.err" || { cat "$work/rpm-$rpmarch.err" >&2; exit 1; }
		cp "$top"/RPMS/"$rpmarch"/*.rpm "dist/openvibe-link-${pkgver}-1.${rpmarch}.rpm"
		echo "built dist/openvibe-link-${pkgver}-1.${rpmarch}.rpm"
	done
else
	echo "package: rpmbuild is not installed here; skipped the .rpm (CI installs rpm and builds it)" >&2
fi

echo "packages ready for $VERSION"
