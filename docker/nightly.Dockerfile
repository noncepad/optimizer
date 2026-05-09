FROM debian:trixie-slim

RUN apt-get update \
  && apt-get install -y wget gnupg \
  && wget -O- https://noncepad.com/keys/nightly.bookworm.sources > /etc/apt/sources.list.d/noncepad.nightly.sources \
  && apt-get update \
  && apt-get install -y solpipe \
  && useradd -ms /bin/bash solpiper 

USER solpiper

CMD ["/bin/bash"]
