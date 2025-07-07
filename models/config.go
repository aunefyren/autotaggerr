package models

type ConfigStruct struct {
	Timezone                string `json:"timezone"`
	PrivateKey              string `json:"private_key"`
	TreninghetenPort        int    `json:"treningheten_port"`
	TreninghetenName        string `json:"treningheten_name"`
	TreninghetenExternalURL string `json:"treningheten_external_url"`
	TreninghetenVersion     string `json:"treningheten_version"`
	TreninghetenEnvironment string `json:"treningheten_environment"`
	TreninghetenTestEmail   string `json:"treningheten_test_email"`
	TreninghetenLogLevel    string `json:"treningheten_log_level"`
	SMTPEnabled             bool   `json:"smtp_enabled"`
	SMTPHost                string `json:"smtp_host"`
	SMTPPort                int    `json:"smtp_port"`
	SMTPUsername            string `json:"smtp_username"`
	SMTPPassword            string `json:"smtp_password"`
	SMTPFrom                string `json:"smtp_from"`
}
