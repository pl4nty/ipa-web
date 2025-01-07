FROM alpine@sha256:b97e2a89d0b9e4011bb88c02ddf01c544b8c781acf1f4d559e7c8f12f1047ac3
COPY ipa-web /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]