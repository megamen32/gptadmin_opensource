.PHONY: build clean install

build:
	go build -o gptadmin-cloudos-nativehost .

clean:
	rm -f gptadmin-cloudos-nativehost

install: build
	cp gptadmin-cloudos-nativehost /usr/local/bin/
