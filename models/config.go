package models

type ConfigStruct struct {
	Timezone                       string   `json:"timezone"`
	PrivateKey                     string   `json:"private_key"`
	AutotaggerrPort                int      `json:"autotaggerr_port"`
	AutotaggerrName                string   `json:"autotaggerr_name"`
	AutotaggerrExternalURL         string   `json:"autotaggerr_external_url"`
	AutotaggerrVersion             string   `json:"autotaggerr_version"`
	AutotaggerrEnvironment         string   `json:"autotaggerr_environment"`
	AutotaggerrTestEmail           string   `json:"autotaggerr_test_email"`
	AutotaggerrLogLevel            string   `json:"autotaggerr_log_level"`
	AutotaggerrLibraries           []string `json:"autotaggerr_libraries"`
	AutotaggerrProcessOnStartUp    bool     `json:"autotaggerr_process_on_start_up"`
	AutotaggerrProcessCronSchedule string   `json:"autotaggerr_process_cron_schedule"`
	SMTPEnabled                    bool     `json:"smtp_enabled"`
	SMTPHost                       string   `json:"smtp_host"`
	SMTPPort                       int      `json:"smtp_port"`
	SMTPUsername                   string   `json:"smtp_username"`
	SMTPPassword                   string   `json:"smtp_password"`
	SMTPFrom                       string   `json:"smtp_from"`
}
