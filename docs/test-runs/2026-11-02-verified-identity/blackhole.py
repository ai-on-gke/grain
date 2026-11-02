import socket
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(("127.0.0.1",18081));s.listen(50)
c=[]
while True: c.append(s.accept())
