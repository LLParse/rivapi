FROM alpine:3.6

RUN apk add --update --no-cache git
ADD bin/rivapi /usr/bin/
ENTRYPOINT ["/usr/bin/rivapi"]
