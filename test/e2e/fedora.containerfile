# A fresh Fedora machine for the end-to-end tests (see e2e_test.go): systemd,
# D-Bus, sudo and a user in the wheel group. Everything else is maisys's to
# install, so the tests catch a dependency it doesn't install.
ARG VERSION=44
FROM registry.fedoraproject.org/fedora:${VERSION}

# systemd and other programs know they're in a container from this.
ENV container=podman
ENV LANG=C.UTF-8

RUN dnf -y install --setopt=install_weak_deps=False \
        ca-certificates \
        dbus \
        shadow-utils \
        sudo \
        systemd \
    && dnf clean all

# Don't start what a container has no use for.
RUN systemctl set-default multi-user.target \
    && systemctl mask \
        console-getty.service \
        dev-hugepages.mount \
        getty.target \
        sys-fs-fuse-connections.mount \
        systemd-remount-fs.service \
        systemd-update-utmp.service

# The test user, in the wheel group like the first user of a fresh install.
# The tests run maisys without a terminal, so sudo can't ask for a password:
# maisys-setup lets tester use sudo without one until the first sync has set
# up the sudo module's own rule, /etc/sudoers.d/tester, and the tests delete
# it. A WSL distro maisys creates gets the same temporary rule.
RUN useradd --create-home --shell /bin/bash --groups wheel tester \
    && echo 'tester ALL=(ALL) NOPASSWD: ALL' >/etc/sudoers.d/maisys-setup \
    && chmod 0440 /etc/sudoers.d/maisys-setup

# systemd stops on SIGRTMIN+3; SIGTERM makes it restart
# (https://systemd.io/CONTAINER_INTERFACE/).
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
