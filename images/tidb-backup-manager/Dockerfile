FROM pingcap/alpine-glibc:alpine-3.14
ARG RCLONE_VERSION=v1.51.0
ARG SHUSH_VERSION=v1.4.0
ARG TOOLKIT_VERSION=v5.2.1
RUN apk update && apk add ca-certificates wget unzip bind-tools --no-cache && rm -rf /var/cache/apk/*

RUN wget -nv https://github.com/ncw/rclone/releases/download/${RCLONE_VERSION}/rclone-${RCLONE_VERSION}-linux-amd64.zip \
  && unzip rclone-${RCLONE_VERSION}-linux-amd64.zip \
  && mv rclone-${RCLONE_VERSION}-linux-amd64/rclone /usr/local/bin \
  && chmod 755 /usr/local/bin/rclone \
  && rm -rf rclone-${RCLONE_VERSION}-linux-amd64.zip rclone-${RCLONE_VERSION}-linux-amd64

RUN wget -nv https://github.com/realestate-com-au/shush/releases/download/${SHUSH_VERSION}/shush_linux_amd64 \
  && mv shush_linux_amd64 /usr/local/bin/shush \
  && chmod 755 /usr/local/bin/shush

RUN \
  wget -nv https://download.pingcap.org/tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64.tar.gz \
  && tar -xzf tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64.tar.gz \
  && mv tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64/bin/tidb-lightning /tidb-lightning \
  && mv tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64/bin/tidb-lightning-ctl /tidb-lightning-ctl \
  && mv tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64/bin/dumpling /dumpling \
  && chmod 755 /dumpling /tidb-lightning /tidb-lightning-ctl \
  && rm -rf tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64.tar.gz \
  && rm -rf tidb-toolkit-${TOOLKIT_VERSION}-linux-amd64

COPY bin/tidb-backup-manager /tidb-backup-manager
COPY entrypoint.sh /entrypoint.sh


ENTRYPOINT ["/entrypoint.sh"]
