FROM scratch
ADD ingress-dns /
ENTRYPOINT [ "/ingress-dns" ]