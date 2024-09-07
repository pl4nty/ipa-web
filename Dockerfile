FROM alpine@sha256:beefdbd8a1da6d2915566fde36db9db0b524eb737fc57cd1367effd16dc0d06d
COPY ipa-web /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]