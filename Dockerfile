FROM alpine@sha256:1e42bbe2508154c9126d48c2b8a75420c3544343bf86fd041fb7527e017a4b4a
COPY ipa-web /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]