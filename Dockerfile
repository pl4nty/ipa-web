FROM alpine@sha256:7eccd4d5d574a3c889b0a6d97b2cdd0308c8e1afc2bba8d467c2b87d879b0c1c
COPY ipa-web /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]