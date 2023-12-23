FROM scratch
COPY example /usr/bin/ipa-web
ENTRYPOINT ["/usr/bin/ipa-web"]