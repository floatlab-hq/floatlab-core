FROM nixos/nix@sha256:ff480dbb23511aa1c10710aa068ceea5b93fed9bfce5f5379f3a6b07282cbc7b

# Docker exec requires account files inside the container's root filesystem.
RUN cp /etc/passwd /tmp/passwd && cp /etc/group /tmp/group \
    && rm /etc/passwd /etc/group \
    && mv /tmp/passwd /etc/passwd && mv /tmp/group /etc/group
