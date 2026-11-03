terraform {
  required_version = ">= 1.3.0"
  required_providers {
    gitlab = {
      source  = "gitlabhq/gitlab"
      version = "18.0.0"
    }
  }
}

provider "gitlab" {
  base_url = var.gitlab_base_url
  token    = var.gitlab_token
}

module "groups" {
  source       = "./modules/create_gitlab_group"
  create_group = var.infrastructure
}

module "projects" {
  source         = "./modules/create_gitlab_project"
  infrastructure = var.infrastructure
  group_ids      = module.groups.group_id
}

module "members" {
  source         = "./modules/add_members_in_group"
  infrastructure = var.infrastructure
  group_ids      = module.groups.group_id
}
