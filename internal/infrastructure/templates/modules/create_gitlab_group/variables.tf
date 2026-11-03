terraform {
  required_providers {
    gitlab = { source = "gitlabhq/gitlab" }
  }
}

variable "create_group" {
  type = map(object({
    parent_group     = optional(string)
    description      = optional(string, "")
    visibility_level = optional(string, "private")
  }))
}
