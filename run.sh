docker run -d \
  --user="root" \
  --name iperf3_exporter_vn\
  --network host \
  --cap-add=NET_ADMIN \
  --cap-add=NET_RAW \
  --restart unless-stopped \
  iperf3_exporter \
  --web.listen-address=:9579
