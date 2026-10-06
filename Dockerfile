FROM gcr.io/distroless/static:nonroot
COPY --chmod=0555 bin/egress-proof-probe /egress-proof-probe
ENTRYPOINT ["/egress-proof-probe"]
