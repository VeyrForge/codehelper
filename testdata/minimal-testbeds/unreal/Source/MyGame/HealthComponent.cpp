#include "HealthComponent.h"

UHealthComponent::UHealthComponent()
{
	PrimaryComponentTick.bCanEverTick = false;
	CurrentHealth = MaxHealth;
}

void UHealthComponent::ApplyDamage(float Amount)
{
	CurrentHealth -= Amount;
	if (CurrentHealth < 0.f)
	{
		CurrentHealth = 0.f;
	}
}
