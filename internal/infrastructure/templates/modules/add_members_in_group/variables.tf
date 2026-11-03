terraform {
  required_providers {
    gitlab = { source = "gitlabhq/gitlab" }
  }
}

variable "group_ids" {
  type = map(string)
}

variable "infrastructure" {
  type = map(object({
    members = optional(map(string), {})
  }))
}
