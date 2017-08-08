FROM alpine:3.6

RUN apk add --update --no-cache git
ADD bin/registryranch /usr/bin/
ENTRYPOINT ["/usr/bin/registryranch"]
