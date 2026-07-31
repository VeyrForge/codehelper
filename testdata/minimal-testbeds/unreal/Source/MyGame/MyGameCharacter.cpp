#include "MyGameCharacter.h"

AMyGameCharacter::AMyGameCharacter()
{
	HealthComp = CreateDefaultSubobject<UHealthComponent>(TEXT("HealthComp"));
}

void AMyGameCharacter::BeginPlay()
{
	Super::BeginPlay();
	if (UHealthComponent* Comp = Cast<UHealthComponent>(HealthComp))
	{
		Comp->ApplyDamage(0.f);
	}
}

void AMyGameCharacter::TakeDamage(float Amount)
{
	if (HealthComp)
	{
		HealthComp->ApplyDamage(Amount);
	}
	Health -= Amount;
	if (Health < 0.f)
	{
		Health = 0.f;
	}
}
