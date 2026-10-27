package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bridge", func() {
	Describe("bridgeBaseURL", func() {
		DescribeTable("accepts valid base URLs",
			func(raw, expected string) {
				endpoint, err := bridgeBaseURL(raw)
				Expect(err).ToNot(HaveOccurred())
				Expect(endpoint).To(Equal(expected))
			},
			Entry("plain http with port", "http://10.0.0.5:8090", "http://10.0.0.5:8090"),
			Entry("trailing slash", "http://10.0.0.5:8090/", "http://10.0.0.5:8090"),
			Entry("https with a path prefix", "https://bridge.example/status", "https://bridge.example/status"),
		)

		DescribeTable("rejects unusable URLs",
			func(raw string) {
				_, err := bridgeBaseURL(raw)
				Expect(err).To(HaveOccurred())
			},
			Entry("empty", ""),
			Entry("other scheme", "ftp://10.0.0.5"),
			Entry("no host", "http://"),
			Entry("credentials", "http://user:pass@10.0.0.5:8090"),
			Entry("query", "http://10.0.0.5:8090?x=1"),
		)
	})

	Describe("sign", func() {
		It("matches a reference HMAC-SHA256 over timestamp.nonce.body", func() {
			signature := sign([]byte("0123456789abcdef0123456789abcdef"),
				"1700000000", "00112233445566778899aabbccddeeff", []byte(`{"v":1}`))
			Expect(signature).To(Equal("cc5dea683ee6cc8c872dad6d45d8d07c02df0da728efa619ecd3eb72635d504a"))
		})
	})

	Describe("newNonce", func() {
		It("returns distinct 32-character hex strings", func() {
			first, err := newNonce()
			Expect(err).ToNot(HaveOccurred())
			second, err := newNonce()
			Expect(err).ToNot(HaveOccurred())

			Expect(first).To(MatchRegexp("^[0-9a-f]{32}$"))
			Expect(first).ToNot(Equal(second))
		})
	})
})
