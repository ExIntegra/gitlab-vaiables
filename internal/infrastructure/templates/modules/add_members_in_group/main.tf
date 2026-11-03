locals {
  members = { for member in flatten([
    for group_name, group in var.infrastructure : [
      for username, role in group.members : {
        group_name = group_name
        username   = username
        role       = role
      }
    ]
  ]) : "${member.group_name}/${member.username}" => member }
}

data "gitlab_user" "users" {
  for_each = toset([for member in values(local.members) : member.username])
  username = each.value
}

resource "gitlab_group_membership" "members" {
  for_each     = local.members
  group_id     = var.group_ids[each.value.group_name]
  user_id      = data.gitlab_user.users[each.value.username].id
  access_level = each.value.role
}
