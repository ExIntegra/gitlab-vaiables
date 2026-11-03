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
    projects = optional(map(object({
      description            = optional(string, "")
      visibility_level       = optional(string, "private")
      initialize_with_readme = optional(bool, true)
      default_branch         = optional(string, "main")
      merge_method           = optional(string, "merge")
      protected_branches = optional(map(object({
        push_access_level  = optional(string, "no one")
        merge_access_level = optional(string, "maintainer")
        allow_force_push   = optional(bool, false)
      })), {})
    })), {})
  }))
}
