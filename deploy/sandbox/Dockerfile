FROM alpine:3.20

RUN apk add --no-cache \
	openssh-server \
	git \
	ripgrep \
	jq \
	yq-go \
	sed \
	findutils \
	coreutils \
	bash

RUN adduser -D -h /work sandbox

RUN ssh-keygen -A \
 && sed -i 's/#PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config \
 && sed -i 's/#PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && echo "AllowUsers sandbox" >> /etc/ssh/sshd_config

RUN mkdir -p /repos /work /tmp
WORKDIR /work

USER sandbox
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
