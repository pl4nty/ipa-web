FROM alpine@sha256:56fa17d2a7e7f168a043a2712e63aed1f8543aeafdcee47c58dcffe38ed51099
COPY ipa-web /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]