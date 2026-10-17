FROM ubuntu:latest
USER root
ADD https://example.com/install.sh /tmp/install.sh
RUN chmod 777 /tmp/install.sh && /tmp/install.sh
EXPOSE 22
CMD ["/bin/bash"]
