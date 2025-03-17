FROM golang:1.23.7 AS builder

LABEL stage=gobuilder
ENV CGO_ENABLED 0
ENV GOOS linux

RUN apt update && apt install tzdata

WORKDIR /app
# COPY . .
# RUN chmod +x ./bash/install
# RUN bash ./bash/install
# RUN bash ./bash/build

RUN apt install git && git clone https://github.com/pkg-ru/imager.git
RUN cd imager && chmod +x ./bash/install && bash ./bash/install && bash ./bash/build

######################################
FROM alpine

RUN apk update --no-cache && apk add --update bash && apk add ffmpeg imagemagick libde265 libheif
COPY --from=builder /usr/share/zoneinfo/Europe/Moscow /usr/share/zoneinfo/Europe/Moscow
ENV TZ Europe/Moscow

WORKDIR /app
# COPY --from=builder /app/imager /app/imager
# COPY --from=builder /app/setting.yaml /app/setting.yaml
COPY --from=builder /app/imager/imager /app/imager
COPY --from=builder /app/imager/setting.yaml /app/setting.yaml

RUN chmod 0777 /app
RUN chmod 0755 /app/imager && chmod +x /app/imager

CMD ["/app/imager"]